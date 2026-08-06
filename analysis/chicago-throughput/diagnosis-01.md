# Diagnosis 01 — Chicago oversaturation dead-stop

Date: 2026-08-05. Analyst: automated diagnosis pass (scratch: `/tmp/chidiag/*.py`).
Artifacts: `data/runs/drain-chi-adaptive-final/base-s42.{json,log}` (216,000 ticks, 6 sim-h),
`data/runs/chi-half-base.{metrics,runreport}.json` (54,000 ticks, 90 min),
`data/scenarios/chi-loop-urban-half-base/{chi-loop-urban.json,demand/main.yaml}`,
plus bracket logs `data/runs/whatif-chi-half/base-s100{0..3}.log` and the older
`data/runs/drain-chi-{base,adaptive}/base-s42.log` for contrast.

**Headline: the two artifact families tell different stories, and one of them is void.**
All three 216k-tick seed-42 "drain" runs (`drain-chi-base`, `drain-chi-adaptive`,
`drain-chi-adaptive-final`) were run with the default driver at **claim capacity 4**
against a fleet that peaks at ~7,600 vehicles. 99.94% of vehicle-ticks ran with no
controller intent — the kernel's hold-last branch sets `Acc = 0` with no car-following
term (`engine/engine.go:703-705`), so a vehicle that stops once never accelerates again
and nobody lane-changes. The run log itself declares the metrics void
("manufactures overlaps that are NOT traffic congestion"). The physically meaningful
baseline is the 90-minute `chi-half-base` family (bracket seeds 1000–1003 confirm the
same harness family: 8 drivers × 6,000 capacity, coasting 0.01% of vehicle-ticks).

---

## A. Jam geography

Method: stream-parse per-lane 5-min interval records (`/tmp/chidiag/stream_metrics.py`),
then classify a road lane (length ≥ 10 m, non-internal) as *frozen in bucket b* when it
has vehicles aboard (`sum_time_s > 0`) and bucket mean speed < 0.5 m/s
(`/tmp/chidiag/freeze.py`). Section = `section` field of the network lane
(`engine/netfile.go:50`: edge id on roads, `j:<node>` inside junctions).

### chi-half-base (valid physics)

- Freeze is **systemic, not localized**. 376 road lanes frozen at the horizon across
  **337 distinct sections** (top section has only 4 lanes). Frozen lanes are scattered
  across the whole map (centroids from (1080,13165) north to (2757,1663) south), are
  overwhelmingly ordinary 48 km/h arterials, and are **not** sinks: 0 of 376 are exit
  lanes, only 20 are demand-destination lanes.
- **When:** contagion starts immediately and never stops — newly-frozen road lanes per
  5-min bucket: 6 (min 5), 14 (min 20), 22 (min 35), 24 (min 50), 31 (min 65),
  **92 (min 85–90, still accelerating)**. The runreport curve agrees: network mean speed
  43.4 → 21.2 km/h by min 45–50, → 8.9 km/h at min 85–90 and still falling;
  pct-lane-km over critical density crosses 1% around min 40–45.
- **Where (district level, from the runreport):** the congestion mass sits in the core
  grid — south-loop (k=3.38, 1,237 veh·h lost), west-loop (3.17, 621), cbd (3.17, 612),
  near-north (2.07, 441) — while north/west/southwest stay ≤0.91. Freeway corridors
  stay fast all run: Kennedy 74.9, Eisenhower 78.3, Dan Ryan 70.0, Stevenson 55.8 km/h;
  the one bad corridor is **jane-byrne** (13.1 km/h, k=5.94) — the interchange where
  freeway traffic feeds the grid. Arterial grid average: 14.3 km/h.
- Top time-loss lanes (runreport hotspots + `/tmp/chidiag/jamgeo.py`): urban arterial
  lanes in west-loop/south-loop/near-north (e.g. `n313816930_0` jane-byrne, 42.8 veh·h;
  `n514505609_0_d2` south-loop, 33.4 veh·h), not destination lanes and not portals.
- ~2,535 of the 3,553 vehicles active at the horizon stand on frozen road lanes
  (occupancy × length / 5 m estimate).

### drain run (broken harness — listed only to explain it)

- 646 road lanes frozen at horizon across 561 sections, but **172 of them froze in the
  first 5 minutes** and 127 more by min 10 — the signature of coast-and-freeze, not of
  congestion growth. Network mean speed is 25.3 km/h in bucket 0 (vs 43.4 in chi-half)
  and 6.5 km/h by min 10.

## B. Collision counter

- What it counts (`engine/engine.go:1013-1023`, `993-1011`): once per tick, for every
  adjacent vehicle pair on a lane plus the routed cross-boundary pair, if the gap is
  below −0.01 m (`collisionGap`, engine.go:979) one observation is added, bucketed by
  section. **Units are overlapped pair-ticks, not collision events.** A real overlap
  (bodies interpenetrate) is required — but two cars sitting overlapped for an hour
  produce 36,000 "collisions".
- The drain run's 1,889,676 observations decompose as ~15 sections × ~206,000 ≈
  **one overlapped pair per section standing still for ~95% of the 216,000 ticks**
  (top: 4388612 = 209,611/216,000 = 0.97/tick). The safety gate's own counter agrees:
  1,889,684 vehicle-ticks "saw an already-overlapped pair" (log). So ~8–15 persistent
  overlapped pairs, not 1.9M impacts.
- Where: all 8 listed sections are **ordinary high-speed road lanes** (64–88.5 km/h
  freeway/express lanes) scattered across the map — e.g. 4388612 (6849,14742, far north),
  124471101 (6667,4653), 1306962144 (7288,3363), 918672679 (9784,1820)
  (`/tmp/chidiag` section lookup against the network file). They are queue tails that
  fast coasting vehicles plowed into — the exact pathology the SafetyDecel comment
  documents (`engine/engine.go:31-38`): hold-last sets `Acc = 0` with no car-following,
  so an uncontrolled vehicle "holds its speed into a standing queue and stays overlapped
  for the rest of the run." Once both vehicles are stopped with `Acc = 0`, nothing ever
  separates the pair.
- The gate (run at 6.0 m/s², log line) cannot prevent all of these: boundary crossing is
  a **placement**, not a motion (`engine/engine.go:909-914`) — 70 landings arrived
  overlapped, beyond the reach of any acceleration guardrail.
- Is it real physics violation or noise? **Real overlaps, but manufactured by the
  harness, not by congestion.** Evidence: in the healthy chi-half run, zero lanes ≥ 20 m
  ever reached occupancy 0.9 in any 5-min bucket; the drain run has 57 such lanes,
  up to occ 1.05 — physically impossible without interpenetration
  (`/tmp/chidiag` high-occupancy scan). Healthy bracket seeds show 995–1,880 total
  observations over ~83 sections (≈0.005% of pair-ticks) — noise level. The physics does
  not admit meaningful overlap under proper control; 1.9M is a driver-starvation
  artifact, not evidence of broken car-following in the valid runs.

## C. Despawn accounting

Code paths:

- Boundary exit lane end: `Despawned++` (`engine/engine.go:889-893`).
- Destination arrival: a vehicle whose `Route` equals the lane it just finished
  despawns with `Despawned++` **and** `Arrived++` (engine.go:894-905) — arrival happens
  at the **end** of the destination lane.
- Gridlock escape: `strandStuck` (engine/gridlock.go:81-143) removes a vehicle that has
  been stopped > `StrandAfterS` (300 s, engine.go:88) **at the head of its lane** at a
  blocked junction box; counted in `Stats.Stranded`, trip emitted `completed=false,
  stranded=true` (metricsjson.go:56-60). **Not** in despawned, **not** in
  completed_trips.
- Metrics `completed_trips` = trips that left the network normally (exit + arrival);
  `stranded_trips` = escape removals (metrics.go:181-185); `active_at_horizon` = the
  rest. Disjoint and exhaustive.

Reconciliation (both close exactly — no vehicle vanishes unaccounted):

- drain: spawned 9,171 = despawned 786 (784 exit + **arrived 2**) + stranded 2,085 +
  active 6,300 ✓ (log + totals json)
- chi-half: injected 10,641 = completed 6,144 (4,207 exit-bound + 1,937 workplace
  arrivals) + stranded 944 + active 3,553 ✓

`dropped_crossings` (292,494 in chi-half) is **not dropped vehicles**: it counts ticks
where the trip tracker's successor-chain resolution failed or was ambiguous after a
lane change (`engine/metrics.go:1054-1061`, doc at 194-200). Distance is still booked
via a defensive split; it is a metrics-attribution fidelity counter (netimport networks
with sub-vehicle-length internal lanes), and the exact mass balance above proves no
loss. That it is 292k while the coasting drain run shows only 3,581 is consistent:
healthy runs lane-change ~95,000 times (bracket logs), the coasting run 3,480.

The gridlock escape therefore **does remove vehicles** (by design, loudly, counted as
stranded) — 944 in the healthy 90-min run, ~1,000 per healthy bracket seed — and they
are never counted as completed. No teleportation path exists.

## D. Completion timeline (chi-half-base)

Per-5-min completions from the trips list (`/tmp/chidiag/stream_metrics.py`):

- Ramp: 63, 118, 209, 282, 334, 388, **peak 459–487 at min 30–40 (≈5,500–5,840 veh/h)**,
  then a steady decline: 455, 445, 474, 406, 389, 384, 336, 310, 312, 293 at min 85–90
  (≈3,516/h). Completions are **not** front-loaded — half happen after ~min 40 — but the
  discharge rate decays ~15%/hour after the peak while the grid accumulates.
- Completed trips (6,144): mean duration 1,350 s (p10 134, p50 1,229, p90 2,645,
  p99 3,987, max 5,170); mean distance 11.3 km → realized 30 km/h. Mean time-loss 646 s
  → free-flow ≈ 11.7 min vs realized mean 22.5 min (~1.9×); p90 is 44 min.
- Vehicles active at the horizon (3,553): median 50 min already in network, p90 70 min,
  max the full 90; **3,540 of 3,553 are workplace-bound** (13 exit-bound).
- Drain run (void, for contrast): 786 completions, 783 of them before min 60 — the
  network dies with the coasting fleet, not with demand.

## E. Capacity estimate — is ~6k/h a sink limit or an internal limit?

**Internal. The sinks are wide open.**

- Arrival semantics confirmed: completion = reaching the **end** of the destination lane
  (exit lanes: lane end = network boundary; workplace lanes: arrival despawn at lane
  end, engine.go:889-905).
- Demand split (`/tmp/chidiag/demand.py` over main.yaml): 478 destination lanes —
  **90 exit lanes carry 66.7% of expected trips (7,025 of 10,538)**, 388 workplace lanes
  carry 33.3%. Peak summed rate 12,798 veh/h at t=1800–2400 s.
- Sink-side headroom: exit destination lanes are freeway-class (v0 17.9–24.6 m/s).
  The busiest single exit lane expects ~158 veh over the whole hour (~320/h at peak)
  against ~1,800–2,000 veh/h/lane — **<20% utilized**; aggregate exit-lane capacity
  ~90 × 1,900 ≈ 170k/h vs ~8.5k/h exit-bound peak demand. Workplace lanes average
  ~9 expected trips/h each.
- Measured, not just theoretical: at the chi-half horizon every top exit-destination
  lane is **free-flowing and empty** (16–27 m/s, occupancy ≈ 0.00–0.01; freeze.py
  output). Exit-bound trips essentially all get out: 4,207 completed, **0 stranded**,
  13 active at horizon. Freeway corridors run 43–78 km/h all run.
- The entire residual backlog is workplace-bound: of 6,421 workplace-bound injected
  trips, only 1,937 (30%) completed; 944 stranded; 3,540 still inside at 90 min.
- Conclusion: the realized discharge (peak ≈5.8k/h, average 4.1k/h) is bounded by the
  **signalized core grid's ability to absorb and transit the workplace-bound third of
  demand** (fixed-time programs, no actuated control in the base arm, no entry
  metering), with queue spillback sealing junction boxes — not by the number or speed
  of exit portals. The demand profile is ~2× what the *grid* can pass, not 2× what the
  *sinks* can take.

## F. Backlog / where the accepted verbs went

- Director → kernel: every accepted verb enters a hold-and-retry injection queue; a
  verb whose earliest tick has passed retries each tick until the origin clears, up to
  `DirectorSpawnHoldTicks = 600` ticks (60 s) past its earliest tick, then **expires —
  dropped, counted in `DirExpired`** (`engine/director.go:18-23,32-49,244-260`;
  queue depth observable via `PendingSpawns()`, director.go:212-214).
- Drain run: verbs 10,698 = injected 9,171 + **expired 1,527** (log: "injected=9171
  expired=1527 (86% delivered) lastInject=tick 35998 firstExpire=tick 5220"). Nothing
  is pending at the horizon: the demand program ends at tick 36,000 and the hold window
  closes 600 ticks later; `denied_pending = 0` in totals.
- Expiries cluster on 66 origin lanes; worst `n187346218_0` (91) — a 172 m, 20 m/s
  ramp origin with 112 expected vehicles, blocked by ramp backup. Top-10 expiry lanes
  are all short ramp/interior origins feeding loaded corridors.
- chi-half (healthy): expired only 57 of 10,698 (99.5% delivered) — delivery is fine
  when the physics runs properly; the drain run's 14% loss is the coasting fleet
  blocking its own origins.
- `denied_served` (vehicles held ≥1 tick then injected/expired): 1,028 chi-half vs
  1,725 drain; `denied_wait_s` 8,962 vs 92,416 veh·s — same story.

---

## Top 3 candidate root causes (ranked)

1. **Harness driver starvation voids every 216k-tick drain artifact.** All three
   drain-chi runs attached one default driver with **capacity 4** for a ~7,600-vehicle
   fleet: 99.94% of vehicle-ticks coasted uncontrolled (stop once → never move again,
   no lane changes, manufactured standing overlaps — the 1.9M "collisions" are ~15
   pairs parked inside each other). 172 lanes were frozen by minute 5. The "seed-42
   dead-stop on schedule" evidence behind ADR-0036's 2×-oversaturation verdict and the
   mission's "6,300 frozen" headline is a harness artifact. **Action: rerun the drain
   scenario with the bracket configuration (e.g. `-drivers 8 -capacity 48000`, as
   `scripts/chicago/fwsweep.sh:78` and the whatif arms use) before touching physics,
   demand, or signals.** Note ADR-0036's "no effect in the 2× regime" conclusion rests
   on comparing two equally void arms.
2. **Workplace-bound third of demand vs the fixed-time signalized core grid.** Sinks
   are demonstrably not the limit (exit lanes empty and free-flowing at the horizon;
   0 exit-bound strandings; freeway corridors at 43–78 km/h throughout). The backlog is
   100% workplace-bound trips into south-loop/west-loop/cbd, where fixed-time signals
   with no actuated control and an open-loop demand director (no metering — see
   `docs/chicago-throughput-log.md` open question 3) let queues spill back and seal
   junction boxes. Accumulation starts ~min 20–25, discharge peaks at 5.8k/h around
   min 35–40 and decays thereafter. This is the real throughput problem to fix
   (metering/gating/actuation — ADR-0037 territory).
3. **Gridlock forms early and the escape only bleeds it.** ~1,000 strandings per
   healthy run (9–10% of all trips, 100% workplace-bound), with the first removals at
   min 5–10 — since `StrandAfterS = 300 s`, some junction boxes were continuously
   sealed from the first demand wave (t=0 demand is already 7.4k/h). Either specific
   boxes seal almost instantly under load (geometry/signal-program defect worth its own
   investigation — 944 strands over ~285 sections, top sections in the drain log are
   freeway-feeder arterials like 918574367 (10251,1128) and 915824520 (810,6535)) or
   the startup wave itself overloads them. The escape converts deadlock into counted
   incomplete trips but removes only lane heads, one per 300 s per lane — it masks
   deadlock magnitude without draining the core.
