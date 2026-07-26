# Chicago Metro — zones, import pipeline, demand method

Status: active (phase 1, 2026-07-24, branch `chicago-metro`).
Scope: metro partitioned into named zones; each zone is an independently
imported, independently simulated network. Full-metro single network is an
explicit non-goal (compiled JSON ≈ 0.7 KB/lane; keyframe payload wall at
13.6k vehicles; kernel ~7 ms/tick at 8k vehicles gridlocked).

## Pipeline (ADR-0009 §4 Geofabrik mode, now implemented)

Source of truth: `scripts/chicago/zones.geojson` — display polygons
(districts form a loose partition; corridors overlay) with `name`, `label`,
`kind` (`district`|`corridor`), `status` (`runnable`|`import-pending`).

1. Download state PBF: Geofabrik `illinois-latest.osm.pbf`, md5-pinned
   (`data/networks/chicago/illinois-260723.osm.pbf`, 355 MB).
2. `scripts/chicago/extract.py --zone NAME --buffer-m M --highway-regex RE`
   — polygon extract buffered in UTM 16N; keeps highway ways intersecting
   the buffer + their turn restrictions; `remove_tags=False` is LOAD-BEARING
   (pyosmium's BackReferenceWriter strips tags from completed nodes by
   default, which silently deletes every `highway=traffic_signals` node —
   found 2026-07-24: first chi-loop import compiled 0 signal programs from
   1,145 source signal nodes).
3. `netconvert --osm-files z.osm -o z.net.xml --proj.utm --no-turnarounds
   --junctions.join -t scripts/osm-urban-us.typ.xml` (pinned flags per
   network-format-v1.md + prior imports). **The typemap is not optional for
   a US import** (ADR-0022): netconvert's built-in OSM typemap is
   German-derived and defaults unposted `secondary`/`primary` to 27.78 m/s
   (100 km/h), and `maxspeed` is tagged on only ~16% of chi-loop's secondary
   ways — so the first chi-loop import ran Michigan Avenue, Wells, Wabash
   and State Street at 62 mph. The override changes ONLY the `speed`
   attribute of the 38 stock types (verified attribute-by-attribute:
   numLanes, priority, oneway and the disallow lists are byte-identical) and
   an OSM `maxspeed` tag still wins. **A speed-only typemap does NOT mean a
   speed-only import**: netconvert derives right-of-way FROM speed, and
   30 mph (13.41 m/s) sits 0.2 m/s under the
   `--junctions.right-before-left.speed-threshold` default (13.6111 m/s), so
   the first re-import silently retyped 1,218 Chicago intersections
   `priority` → `right_before_left`. The recipe therefore also needs
   `--junctions.right-before-left.speed-threshold 0` (ADR-0022 §6).
   `scripts/import-city.sh` wires both in via `TYPEMAP=`; set that variable
   to empty for a non-US import.
4. `netimport` → `data/networks/chi-NAME/` with import-report.json.
5. `engine/cmd/portals -net N.json -netxml N.net.xml` → portals.json:
   per-lane origin/exit inventory with OSM class (the import report has
   only counts). Fragments flagged (<30 m).
6. `scripts/chicago/mkdemand.py --portals portals.json --total V` →
   demand YAML: class-weighted veh/h per origin lane, scaled to a zone
   total; fragments + minor classes skipped.
7. Scenario dir per zone under `data/scenarios/chi-*/` (vendored network
   copy, scenario.yaml, demand/, README with the napkin derivation).
8. `scripts/chicago/buildings.py --zone NAME --network N.json --road-osm
   N.osm` → `buildings.geojson` (WGS84 overlay, all kinds) +
   `buildings.json` (the demand index: residential/workplace kind, resolved
   levels, floor area, and the snapped ACCESS LANE per building). Lane
   eligibility excludes junction internals, <30 m fragments, and
   motorway/trunk classes — a garage has no driveway onto the Kennedy;
   without that filter 1.6% of buildings snap to freeway lanes. Levels come
   from `building:levels`, else `height`/3.5, else a per-kind default;
   `building=yes` (71% of footprints) is `other` unless an office/shop/
   amenity tag or ≥8 levels promotes it. chi-loop: 42,397 footprints →
   9,164 indexed (6,738 residential, 2,426 workplace, 940 unsnapped).
9. `scripts/chicago/mkod.py --buildings buildings.json --network N.json
   --portals portals.json --total V --resident-share F` → ADR-0021 OD
   demand: portal inflow AND residential interior origins (`offset_m`),
   both carrying floor-area-weighted workplace destinations, on a
   half-hourly AM profile. Supersedes step 6 for zones where trips should
   END inside the zone rather than at the map edge.
10. Overlays for demosrv: copy the zone source of truth
   (`scripts/chicago/zones.geojson`) plus the generated
   `boundaries.geojson` / `water.geojson` into the demosrv `-overlaydir`
   (this repo uses `data/networks/chicago/`). The viz fetches
   `/overlay/{zones,boundaries,water,buildings}.geojson`; a 404 just means
   "no overlay". Overlay artifacts are gitignored data — a fresh checkout
   regenerates or copies them before they render.

## Zones (starter set)

| zone | lanes (+internal) | signals | demand total | notes |
|---|---|---|---|---|
| chi-loop | 23,833 (+31,705) | 2,217 programs | 40,000 veh/h | CBD grid; needs driver exit-routing |
| chi-kennedy | 7,533 (+7,782) | 486 | 18,000 | I-90/94 corridor; TGSIM anchor |
| chi-north-lakefront | 24,189 (+32,185) | 2,082 | 25,000 | residential; households→trips showcase |
| **chi-loop-urban** | 23,833 (+31,587) | 2,217 programs | 9,000 veh/h | chi-loop re-imported with the US urban typemap (ADR-0022); the network `chi-loop-od*` runs on |

Road-class filter per zone: grids `motorway…tertiary` + links (full
residential grid blew up to 391k lanes / 301 MB — 10× envelope);
corridors `motorway…primary` + links.

**chi-loop → chi-loop-urban.** Same OSM extract, same netconvert flags,
only `-t scripts/osm-urban-us.typ.xml` added. Speed-limit distribution over
the 23,837 external lanes:

| road class | lanes | mean, chi-loop | mean, chi-loop-urban |
|---|---|---|---|
| secondary | 15,975 | 55.7 mph (12,945 lanes at 62) | 29.6 mph (none over 50) |
| tertiary | 4,019 | 46.3 mph | 29.4 mph |
| motorway | 1,730 | 53.7 mph | 50.1 mph (88% carry `maxspeed`) |
| primary | 648 | 62.1 mph (all 648) | 30.0 mph (0% carry `maxspeed`) |
| trunk | 299 | 40.1 mph | 38.6 mph |
| **all** | **23,837** | **53.3 mph** | **31.4 mph** |

Lanes over 50 mph: 14,640 → 1,021, and the survivors are motorway lanes
with an explicit OSM `maxspeed`. The 88-mph bucket (192 lanes on
netconvert's 39.44 m/s motorway default) disappears entirely.

Topology is preserved, and on the CORRECTED import (with
`--junctions.right-before-left.speed-threshold 0`) more tightly than the
first attempt managed. Re-measured 2026-07-25:

| | chi-loop | chi-loop-urban |
|---|---|---|
| external lanes | 23,837 | 23,837 (symmetric difference **0**) |
| origins / exits | 246 / 1,432 | 246 / 1,432 |
| signal programs / links | 2,217 / 13,181 | 2,217 / 13,181 |
| junction types | — | 5,104 priority, 2,217 traffic_light, 197 dead_end, **0 right_before_left** |
| internal lanes | 31,705 | 31,722 (+17) |
| connections | 61,229 | 61,246 (+17) |
| yield approaches | 1,089 | 1,106 (+17) |
| conflict pairs | 29,360 | 29,396 (+36) |

The residual ±17 is netconvert's speed-dependent junction-interior
construction (internal-junction split points), which no typemap can hold
fixed. Note the sign: the FIRST import lost 118 internal lanes, because
right-before-left retyping removes internal junctions wholesale. Gaining 17
is the signature of a clean speed-only change. **Recordings on the two
networks are still not comparable at junction level.**

## Demand method

**Phase 1 (presentation load, `mkdemand.py`).** No OD matrices. Portal
injection weighted by road class, totals from napkin anchors (IDOT AADT,
cordon counts, households × peak trip rate). Every vehicle is born at the
map edge and leaves by whichever exit it drifts to — the delay it produces
is diffuse, with no defect lane, because there are no desire lines.

**Phase 2 (building-anchored OD, `mkod.py`, ADR-0021).** Trips start at
residential buildings (interior injection at the access lane, mid-block)
or at portals, and END at workplace buildings, weighted by floor area.
Requires the ADR-0021 kernel work: arrival despawn, `destination`/`offset_m`
on the spawn verb, and the LATERAL route guardrail — without that last
piece route-blind MOBIL walks 92% of routed vehicles off their route
before they arrive (measured, chi-loop).
Each scenario README carries its derivation and is labeled presentation
load until anchored to published counts (TGSIM I-90/94 per-lane rates are
the first real calibration target).

## Engine changes enabling this

- Driver per-vehicle exit routing (`driver.Config.ExitRouting`, serve flag
  `-exit-routing`, default on): the driver assigns each claimed vehicle a
  seeded destination among reachable exit lanes (speed-limit weighted,
  fragments excluded, drawn from the domain-separated
  `traffic-sim/driver/exit-destination` stream — failover-safe), sent once
  as the persistent Route intent; the KERNEL follows it at every
  multi-successor lane via memoized reverse-Dijkstra next-hop tables
  (`engine/routing.go`). Without it grid zones take the kernel
  leftmost-successor default and circulation is garbage. (2026-07-24
  review fix: the first cut sent a one-shot turn intent that steered only
  the first fork — middle successors of 3+-way forks were inexpressible
  through the ±1 turn axis — and ran an O(V²) Dijkstra twice per
  assignment; both removed in favor of engine-side resolution.)
- serve client-attach barrier (`-attach-timeout`, StartGate in natsio):
  run loop parks at tick 0 until embedded driver/director report ready;
  any pace (incl. `-pace 0`) now legal with clients attached. Fast-
  forward calibration runs work: `serve -scenario … -pace 0`.

## Known defects / gotchas

- **Right-before-left retyping rides along with the speed fix if you let
  it.** The statutory 30 mph urban limit is 0.2 m/s under netconvert's
  `--junctions.right-before-left.speed-threshold` default, so a typemap-only
  re-import turns most unsignalized US intersections into
  yield-to-the-right — a known SUMO mutual-blocking gridlock source. Seen in
  the wild before it was diagnosed: a 54,000-tick `chi-loop-od` run on the
  first `chi-loop-urban` import logged **975,673 collision observations**,
  774,172 of them inside one junction (`j:619019057`). Pass
  `--junctions.right-before-left.speed-threshold 0` (ADR-0022 §6).
- **Every scorecard number below `chi-loop`/`chi-kennedy`/
  `chi-north-lakefront` was measured on autobahn speed limits.** Free-flow
  speed is the denominator of delay, so the "delay share" column is
  systematically overstated. Re-import before quoting a level-of-service
  figure (ADR-0022). The old-vs-new comparison that used to sit here was
  measured on the defective first import and has been withdrawn rather than
  re-run: the decision it supported is already made, and the wall-clock is
  better spent on demand numbers than on justifying a settled call.
- **Expressway portals cannot be congested from the zone total.**
  `mkod.py` sets portal rates as `class_rate × (total × portal_share /
  portal_raw)`, so the per-class table is only a shape and `--total` sets
  the level. On chi-loop at `--total 16000` the scale factor is ≈0.24 and
  the Kennedy's two boundary origin lanes inject **337 veh/h/lane** — about
  a sixth of a freeway lane's capacity. The arterial grid saturates near
  16–20k zone total; the freeway portals would need ≈67k to reach their own
  class rate. One scalar cannot congest both, which is why every named
  expressway in `chi-loop-od-30m` runs at 72–79 km/h while N Wells sits at
  8.4. Fix direction: exempt motorway/trunk portals from the zone scaling.

- Cook County boundary missing from boundaries.geojson: its OSM relation
  (122576) never assembles from the Geofabrik Illinois extract (incomplete
  members — lake/state-line ways). City of Chicago + collar counties
  (DuPage, Will) + townships/municipalities render fine. Fix candidates:
  manual polygonize fallback or TIGER source.
- Fragment portals: ~33–45% of origin lanes are <30 m clipping stubs
  (matches the known `_d2` fragment issue); mkdemand skips them.
- Overload melts geometry: chi-kennedy at 600 veh/h/lane flat (≈170k
  veh/h attempted) gridlocked and logged 780 collision observations;
  at sane load (rate 100 or the 18k scenario) zero collisions. Same
  class of behavior as stress-dtla gridlock — demand tuning must avoid
  meltdown, and collision counts belong on the scorecard.
- **Keyframe payload wall confirmed in production (2026-07-24):**
  chi-north-lakefront at 25k veh/h injection crossed ~13.6k live vehicles
  and aborted at tick 18,700 — `log write .log.keyframe: nats: maximum
  payload exceeded`, exactly the BENCHMARKS.md prediction (77 B/veh
  keyframes vs the 1 MB max_payload). The run DIES, it does not degrade.
  Zone demand totals must keep peak fleet under ~13k until keyframe
  chunking lands — that decision is now demo-blocker-adjacent, not just
  benchmark-queue.
- Actuated signals unmodeled (importer limitation) — downtown progression
  is fixed-time approximations.
- **Batch-mode liveness (fixed 2026-07-24):** the contract liveness sweep
  (`DetachAfterTicks`) measures silence in TICKS; at `-pace 0` the engine
  outruns any client's wall-clock reaction, detaches the healthy driver at
  ~tick 10, and the pause gate then wedges the run permanently (0% CPU —
  the signature). The sweep is now skipped when `PaceFloor == 0`
  (`natsio/contract.go`). Lesson: tick-space liveness budgets assume
  pacing keeps tick time near wall time; batch mode breaks the assumption.
- Batch-mode claim lag: at `-pace 0` the driver claims on wall-clock
  latency while the engine rips — in the chi-kennedy tuning run only
  ~40% of spawns were claimed by end of a 10-min window (unclaimed
  vehicles ride the kernel defaults). For scorecard-grade runs where
  full driver control matters, use a finite pace the driver can keep up
  with; unpaced is for throughput, not fidelity. (2026-07-24 review:
  the driver's per-claim routing costs — an O(V²) Dijkstra run twice
  per assignment plus O(network) candidate/reachability rebuilds per
  pick — were likely a major contributor and are now removed/memoized;
  re-measure before assuming the finite-pace workaround is still
  needed.)

## Scorecard (B4 results)

Method: `serve -scenario … -pace 0 -capacity 50000 -metrics-out`, full
simulated hour, aggregated by `scripts/chicago/scorecard.py`.

| zone | demand (veh/h injected) | result | mean speed | delay share |
|---|---|---|---|---|
| chi-kennedy t1 | 16.7k | full hour, 6m41s wall | 71.1 km/h | 16.5% |
| chi-loop t1 | 40k | total gridlock (stress-dtla signature) | 3.8 km/h | 95.2% |
| chi-loop t2 | 20k | aborted tick 24,700 — keyframe payload wall | — | — |
| chi-loop t3 | 10k | full hour; congested but flowing, delay spread across many lanes (no single defect) | 9.2 km/h | 88.3% |
| chi-north-lakefront t1 | 25k | aborted tick 18,700 — keyframe payload wall | — | — |
| chi-north-lakefront t2 | 10.4k | near-gridlock; one arterial parked 15+ min | 7.0 km/h | 91.3% |
| chi-north-lakefront t3 | 4k | flows but hot-spotted; a few lanes parked | 14.0 km/h | 83.0% |

Lakefront t3 proves the grid-zone problem is structural, not volume:
4k veh/h should flow easily, yet delay concentrates on a handful of
parked lanes. Suspects in order: (1) speed-weighted exit routing
concentrates demand on the few fast exits (Lake Shore Drive ramps),
which jam and spill back; (2) netconvert-guessed fixed-time phases
starving arterial approaches; (3) pace-0 claim lag leaving most vehicles
on kernel defaults. Follow-ups: per-exit share caps or demand-weighted
exit weights, signal-program sanity checks, finite-pace scorecard runs,
and the in-zone-destination (OD) work.

Tuning narrative: grid zones gridlock because phase-1 demand is
injection-only with no in-zone trip ends — every vehicle must leave the
map, so steady state requires inflow ≤ exit-ramp throughput, and
speed-weighted exit routing concentrates demand on the few fast exits.
Real cities drain mostly to in-zone destinations (parking); that is the
OD/destination work, not a demand-number tweak. Batch-mode claim lag
(~28–84% of spawns unclaimed at pace 0, size-dependent) means unclaimed
vehicles ride kernel defaults and worsen the clog — scorecard-grade runs
may need a finite pace. Kennedy works because corridor inflow ≈ outflow
by construction.

