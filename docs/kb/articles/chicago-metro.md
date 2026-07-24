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
   --junctions.join` (pinned flags per network-format-v1.md + prior imports).
4. `netimport` → `data/networks/chi-NAME/` with import-report.json.
5. `engine/cmd/portals -net N.json -netxml N.net.xml` → portals.json:
   per-lane origin/exit inventory with OSM class (the import report has
   only counts). Fragments flagged (<30 m).
6. `scripts/chicago/mkdemand.py --portals portals.json --total V` →
   demand YAML: class-weighted veh/h per origin lane, scaled to a zone
   total; fragments + minor classes skipped.
7. Scenario dir per zone under `data/scenarios/chi-*/` (vendored network
   copy, scenario.yaml, demand/, README with the napkin derivation).

## Zones (starter set)

| zone | lanes (+internal) | signals | demand total | notes |
|---|---|---|---|---|
| chi-loop | 23,833 (+31,705) | 2,217 programs | 40,000 veh/h | CBD grid; needs driver exit-routing |
| chi-kennedy | 7,533 (+7,782) | 486 | 18,000 | I-90/94 corridor; TGSIM anchor |
| chi-north-lakefront | 24,189 (+32,185) | 2,082 | 25,000 | residential; households→trips showcase |

Road-class filter per zone: grids `motorway…tertiary` + links (full
residential grid blew up to 391k lanes / 301 MB — 10× envelope);
corridors `motorway…primary` + links.

## Demand method (phase 1 = presentation load)

No OD matrices. Portal injection weighted by road class, totals from
napkin anchors (IDOT AADT, cordon counts, households × peak trip rate).
Each scenario README carries its derivation and is labeled presentation
load until anchored to published counts (TGSIM I-90/94 per-lane rates are
the first real calibration target).

## Engine changes enabling this

- Driver per-vehicle exit routing (`driver.Config.ExitRouting`, serve flag
  `-exit-routing`, default on): seeded per-vehicle destination among
  reachable exit lanes, speed-limit weighted, fragments excluded. Without
  it grid zones take the kernel leftmost-successor default and circulation
  is garbage. Deterministic from (run seed, vehicle ID) — failover-safe.
- serve client-attach barrier (`-attach-timeout`, StartGate in natsio):
  run loop parks at tick 0 until embedded driver/director report ready;
  any pace (incl. `-pace 0`) now legal with clients attached. Fast-
  forward calibration runs work: `serve -scenario … -pace 0`.

## Known defects / gotchas

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
  with; unpaced is for throughput, not fidelity.

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

